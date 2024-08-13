FROM golang:1.23 AS build

ARG TARGETOS
ARG TARGETARCH

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify

COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o app

FROM scratch as runtime
COPY --from=build /build/app /app
USER app

ENTRYPOINT ["/app"]
