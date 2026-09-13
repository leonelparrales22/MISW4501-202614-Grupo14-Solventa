FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/originacion ./cmd/originacion

FROM alpine:3.20
COPY --from=build /out/originacion /originacion
EXPOSE 8080
ENTRYPOINT ["/originacion"]
