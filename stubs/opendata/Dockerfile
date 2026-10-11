# Build context: repository root (uses go.work).
FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/opendata ./stubs/opendata/cmd/opendata

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/opendata /opendata
EXPOSE 9010
ENTRYPOINT ["/opendata"]
