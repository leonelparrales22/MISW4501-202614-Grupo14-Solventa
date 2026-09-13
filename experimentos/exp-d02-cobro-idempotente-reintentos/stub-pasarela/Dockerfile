FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/stub-pasarela ./stub-pasarela

FROM alpine:3.20
COPY --from=build /out/stub-pasarela /stub-pasarela
EXPOSE 9100
ENTRYPOINT ["/stub-pasarela"]
