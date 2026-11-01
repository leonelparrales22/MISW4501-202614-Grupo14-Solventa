# Build context: repository root (uses go.work).
FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/policy-core ./stubs/policy-core/cmd/policy-core

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/policy-core /policy-core
EXPOSE 9060
ENTRYPOINT ["/policy-core"]
