# Build context: repository root (uses go.work).
FROM golang:1.23 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/email ./stubs/email/cmd/email

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/email /email
EXPOSE 9030
ENTRYPOINT ["/email"]
