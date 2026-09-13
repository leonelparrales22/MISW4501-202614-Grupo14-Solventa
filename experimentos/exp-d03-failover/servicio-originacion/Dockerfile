FROM golang:1.24-alpine AS compilacion
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /stub .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 solventa
COPY --from=compilacion /stub /stub
USER solventa
EXPOSE 8080
ENTRYPOINT ["/stub"]
