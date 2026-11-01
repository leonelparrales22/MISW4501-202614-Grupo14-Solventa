# Build context: repository root (uses go.work).
FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/push ./stubs/push/cmd/push

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/push /push
EXPOSE 9070
ENTRYPOINT ["/push"]
