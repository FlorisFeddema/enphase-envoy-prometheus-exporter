package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/golang-jwt/jwt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"
)

var (
	enlightenLoginUrl = "https://enlighten.enphaseenergy.com/login/login.json"
	enlightenTokenUrl = "https://entrez.enphaseenergy.com/tokens"

	envoyProductionV1Url        = "https://%s/api/v1/production"
	envoyProductionInvertersUrl = "https://%s/api/v1/production/inverters"
	envoyCheckJwtUrl            = "https://%s/auth/check_jwt"
	envoyEnsembleInventoryUrl   = "https://%s/ivp/ensemble/inventory"
	envoyHomeJsonUrl            = "https://%s/home.json"
)

func main() {
	username := os.Getenv("EEPE_USERNAME")
	if username == "" {
		log.Fatal("💥 EEPE_USERNAME not set")
	}

	password := os.Getenv("EEPE_PASSWORD")
	if password == "" {
		log.Fatal("💥 EEPE_PASSWORD not set")
	}

	host := os.Getenv("EEPE_HOST")
	if host == "" {
		log.Fatal("💥 EEPE_HOST not set")
	}

	serialNumber := os.Getenv("EEPE_SERIALNUMBER")
	if serialNumber == "" {
		log.Fatal("💥 EEPE_SERIALNUMBER not set")
	}

	sessionId := getSessionId(username, password)
	token := getAuthToken(sessionId, serialNumber, username)
	validateToken(token, serialNumber, username)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf(envoyHomeJsonUrl, host), bytes.NewBuffer(nil))
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", token))

	response, err := client.Do(req)
	if err != nil {
		fmt.Printf("⚠️ Request %s failed", fmt.Sprintf(envoyHomeJsonUrl, host))
		log.Fatal(err)
	}

	text, _ := io.ReadAll(response.Body)
	log.Println(string(text))

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(response.Body)

	log.Println("🏁 Success!")
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

func validateToken(token string, serialNumber string, username string) {
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

	if !valid {
		log.Fatal("💥 Authentication token is not valid")
	}
}
