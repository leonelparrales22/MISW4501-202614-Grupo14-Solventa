# Build context: repository root (uses go.work).
FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/openfinance ./stubs/openfinance/cmd/openfinance

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/openfinance /openfinance
EXPOSE 9000
ENTRYPOINT ["/openfinance"]
