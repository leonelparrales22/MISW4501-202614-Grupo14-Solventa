# Build context: repository root (uses go.work).
FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/pasarela-pagos ./stubs/payment-gateway/cmd/pasarela-pagos

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/pasarela-pagos /pasarela-pagos
EXPOSE 9040
ENTRYPOINT ["/pasarela-pagos"]
