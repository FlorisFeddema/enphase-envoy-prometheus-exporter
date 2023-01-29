package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-co-op/gocron"
	"github.com/golang-jwt/jwt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	enlightenLoginUrl = "https://enlighten.enphaseenergy.com/login/login.json"
	enlightenTokenUrl = "https://entrez.enphaseenergy.com/tokens"
	envoyCheckJwtUrl  = "https://%s/auth/check_jwt"

	envoyProductionUrl          = "https://%s/api/v1/production"
	envoyProductionInvertersUrl = "https://%s/api/v1/production/inverters"
	envoyHomeUrl                = "https://%s/home.json"

	descProductionWatthoursToday            = prometheus.NewDesc("envoy_production_watthours_today", "Amount of watt-hours produced today", nil, nil)
	descProductionWatthoursLifetime         = prometheus.NewDesc("envoy_production_watthours_lifetime", "Amount of watt-hours produced in total lifetime", nil, nil)
	descProductionWattsNow                  = prometheus.NewDesc("envoy_production_watts_now", "Amount of watts currently produced", nil, nil)
	descProductionInverterLastReportedWatts = prometheus.NewDesc("envoy_production_inverter_last_reported_watts", "Last reported amount of watts of an inverter", []string{"serialNumber"}, nil)
	descProductionInverterMaxReportedWatts  = prometheus.NewDesc("envoy_production_inverter_max_reported_watts", "Max reported amount of watts of an inverter", []string{"serialNumber"}, nil)
	descDatabaseSize                        = prometheus.NewDesc("envoy_database_size", "The size of the internal database", nil, nil)
	descDatabasePercent                     = prometheus.NewDesc("envoy_database_percent", "Percentage of the internal database", nil, nil)
	descSystemConnected                     = prometheus.NewDesc("envoy_system_connected", "If the system is connected to the cloud", nil, nil)

	cloudToken string
	client     = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
)

type envoyType struct {
	ProductionType          *envoyProductionType
	ProductionInvertersType *[]envoyProductionInvertersType
	HomeType                *envoyHomeType
}

type envoyProductionType struct {
	WattHoursToday    int `json:"wattHoursToday"`
	WattHoursLifetime int `json:"wattHoursLifetime"`
	WattsNow          int `json:"wattsNow"`
}

type envoyProductionInvertersType struct {
	SerialNumber    string `json:"serialNumber"`
	LastReportWatts int    `json:"lastReportWatts"`
	MaxReportWatts  int    `json:"maxReportWatts"`
}

type envoyHomeType struct {
	DbSize        int    `json:"db_size"`
	DbPercentFull string `json:"db_percent_full"`
	Network       struct {
		WebCom bool `json:"web_comm"`
	} `json:"network"`
}

type Exporter struct {
}

func NewExporter() *Exporter {
	return &Exporter{}
}

func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	ch <- descSystemConnected
	ch <- descDatabaseSize
	ch <- descDatabasePercent
	ch <- descProductionWatthoursLifetime
	ch <- descProductionWatthoursToday
	ch <- descProductionWattsNow
	ch <- descProductionInverterMaxReportedWatts
	ch <- descProductionInverterLastReportedWatts
}

func main() {
	fetchCloudToken()
	s := gocron.NewScheduler(time.UTC)
	_, _ = s.Every(90).Days().Do(fetchCloudToken)
	s.StartAsync()

	exporter := NewExporter()
	prometheus.MustRegister(exporter)

	http.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe(":9000", nil))
}

func (e *Exporter) Collect(metrics chan<- prometheus.Metric) {
	data := fetchSystemData()

	var online float64 = 0
	if data.HomeType.Network.WebCom {
		online = 1
	}
	metrics <- prometheus.MustNewConstMetric(
		descSystemConnected,
		prometheus.GaugeValue,
		online,
	)
	dbPercentFull, _ := strconv.ParseFloat(strings.TrimSpace(data.HomeType.DbPercentFull), 64)
	metrics <- prometheus.MustNewConstMetric(
		descDatabasePercent,
		prometheus.GaugeValue,
		dbPercentFull,
	)
	metrics <- prometheus.MustNewConstMetric(
		descDatabaseSize,
		prometheus.GaugeValue,
		float64(data.HomeType.DbSize),
	)
	metrics <- prometheus.MustNewConstMetric(
		descProductionWatthoursToday,
		prometheus.GaugeValue,
		float64(data.ProductionType.WattHoursToday),
	)
	metrics <- prometheus.MustNewConstMetric(
		descProductionWatthoursLifetime,
		prometheus.GaugeValue,
		float64(data.ProductionType.WattHoursLifetime),
	)
	metrics <- prometheus.MustNewConstMetric(
		descProductionWattsNow,
		prometheus.GaugeValue,
		float64(data.ProductionType.WattsNow),
	)
	for _, inverter := range *data.ProductionInvertersType {
		metrics <- prometheus.MustNewConstMetric(
			descProductionInverterLastReportedWatts,
			prometheus.GaugeValue,
			float64(inverter.LastReportWatts),
			inverter.SerialNumber,
		)
		metrics <- prometheus.MustNewConstMetric(
			descProductionInverterMaxReportedWatts,
			prometheus.GaugeValue,
			float64(inverter.MaxReportWatts),
			inverter.SerialNumber,
		)
	}
}

func fetchSystemData() envoyType {
	host := os.Getenv("EEPE_HOST")
	if host == "" {
		log.Fatal("💥 EEPE_HOST not set")
	}

	cookie := getLocalSessionId(fmt.Sprintf(envoyCheckJwtUrl, host), cloudToken)
	if cookie == nil {
		log.Fatal("💥 Local session id not found")
	}

	var envoyData = envoyType{
		ProductionType:          &envoyProductionType{},
		ProductionInvertersType: &[]envoyProductionInvertersType{},
		HomeType:                &envoyHomeType{},
	}

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		getData(fmt.Sprintf(envoyProductionUrl, host), cloudToken, cookie, envoyData.ProductionType)
		wg.Done()
	}()
	go func() {
		getData(fmt.Sprintf(envoyHomeUrl, host), cloudToken, cookie, envoyData.HomeType)
		wg.Done()
	}()
	go func() {
		getData(fmt.Sprintf(envoyProductionInvertersUrl, host), cloudToken, cookie, envoyData.ProductionInvertersType)
		wg.Done()
	}()
	wg.Wait()

	return envoyData
}

func fetchCloudToken() {
	username := os.Getenv("EEPE_USERNAME")
	if username == "" {
		log.Fatal("💥 EEPE_USERNAME not set")
	}

	password := os.Getenv("EEPE_PASSWORD")
	if password == "" {
		log.Fatal("💥 EEPE_PASSWORD not set")
	}

	serialNumber := os.Getenv("EEPE_SERIALNUMBER")
	if serialNumber == "" {
		log.Fatal("💥 EEPE_SERIALNUMBER not set")
	}

	host := os.Getenv("EEPE_HOST")
	if host == "" {
		log.Fatal("💥 EEPE_HOST not set")
	}

	sessionId := getSessionId(username, password)
	token := getAuthToken(sessionId, serialNumber, username)
	validateToken(token, serialNumber, username, host)
	cloudToken = token
}

func getData(url string, token string, sessionId *http.Cookie, data interface{}) interface{} {
	req, err := http.NewRequest(http.MethodGet, url, bytes.NewBuffer(nil))
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))
	req.AddCookie(sessionId)

	response, err := client.Do(req)
	if err != nil {
		fmt.Printf("⚠️ Request %s failed\n", url)
		log.Fatal(err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		fmt.Printf("️⚠️ Request failed with status %d\n", response.StatusCode)
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(response.Body)

	body, _ := io.ReadAll(response.Body)

	err = json.Unmarshal(body, &data)
	if err != nil {
		log.Fatal("💥 JSON object was not valid")
	}

	return data
}

func getLocalSessionId(url string, token string) *http.Cookie {
	req, err := http.NewRequest(http.MethodGet, url, bytes.NewBuffer(nil))
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))

	response, err := client.Do(req)
	if err != nil {
		fmt.Printf("⚠️ Request %s failed", url)
		log.Fatal(err)
	}

	for _, cookie := range response.Cookies() {
		if cookie.Name == "sessionId" {
			return cookie
		}
	}
	return nil
}

func getSessionId(username string, password string) string {
	type enlightenLoginResponse struct {
		SessionId string `json:"session_id"`
	}

	data := url.Values{"user[email]": {username}, "user[password]": {password}}
	response, err := http.PostForm(enlightenLoginUrl, data)
	if err != nil {
		log.Fatal("💥 Login credentials are incorrect")
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(response.Body)
	body, _ := io.ReadAll(response.Body)

	jsonResponse := enlightenLoginResponse{}
	err = json.Unmarshal(body, &jsonResponse)
	if err != nil {
		log.Fatal("💥 Form login response was not valid json")
	}
	return jsonResponse.SessionId
}

func getAuthToken(sessionId string, serialNumber string, username string) string {
	data := map[string]string{
		"session_id": sessionId,
		"serial_num": serialNumber,
		"username":   username,
	}
	jsonData, _ := json.Marshal(data)

	response, err := http.Post(enlightenTokenUrl, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Fatal("💥 Could not get authentication token, serial number might be incorrect")
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(response.Body)

	token, _ := io.ReadAll(response.Body)
	if err != nil {
		log.Fatal("💥 Could not get a token")
	}
	return string(token)
}

func validateToken(token string, serialNumber string, username string, host string) {
	authToken, _ := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		_, ok := token.Method.(*jwt.SigningMethodECDSA)
		if !ok {
			return nil, errors.New("token not valid")
		}
		return "", nil
	})
	claims, ok := authToken.Claims.(jwt.MapClaims)
	if !ok {
		log.Fatal("💥 Could not get authentication token claims")
	}

	valid := claims.VerifyIssuer("Entrez", true) &&
		claims.VerifyAudience(serialNumber, true) &&
		claims["username"] == username &&
		claims["enphaseUser"] == "owner" &&
		time.Unix(int64(claims["exp"].(float64)), 0).Add(time.Hour*24*30*-1).After(time.Now())

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf(envoyCheckJwtUrl, host), bytes.NewBuffer(nil))
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))

	response, err := client.Do(req)
	if err != nil {
		fmt.Printf("⚠️ Request %s failed", fmt.Sprintf(envoyCheckJwtUrl, host))
		log.Fatal(err)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 || !valid {
		log.Fatal("💥 Authentication token is not valid")
	}
}
