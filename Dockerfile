ARG GO_VERSION=1.26
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/app-server ./cmd/app-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app-server /app-server
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/app-server"]
