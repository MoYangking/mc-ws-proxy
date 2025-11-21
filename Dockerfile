FROM golang:1.21 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /mc-ws-proxy .

FROM scratch

WORKDIR /app
COPY --from=builder /mc-ws-proxy ./mc-ws-proxy

EXPOSE 25565

CMD ["./mc-ws-proxy", "-mode", "entry", "-listen", ":25565", "-ws", "wss://mc.moyang.locker/ws"]
