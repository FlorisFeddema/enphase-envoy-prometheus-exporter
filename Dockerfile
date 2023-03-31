FROM golang:1.20 AS build

WORKDIR /build

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64

COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify

COPY . .
RUN go build -o app

FROM scratch as runtime
COPY --from=build /build/app /app

ENTRYPOINT ["/app"]
