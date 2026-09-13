FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -o /out/originacion ./cmd/originacion
RUN CGO_ENABLED=0 go build -o /out/openfinance ./cmd/openfinance-stub.go
FROM alpine:3.21
COPY --from=build /out/originacion /usr/local/bin/originacion
COPY --from=build /out/openfinance /usr/local/bin/openfinance
