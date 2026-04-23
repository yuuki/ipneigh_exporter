FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w -X main.version=$(cat VERSION 2>/dev/null || echo dev)" -o /ipneigh_exporter .

FROM scratch
COPY --from=builder /ipneigh_exporter /ipneigh_exporter
EXPOSE 9144
ENTRYPOINT ["/ipneigh_exporter"]
