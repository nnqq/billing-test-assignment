FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/...

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/api /api
COPY --from=build /out/importer /importer
COPY testdata/transactions.csv /app/testdata/transactions.csv

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/api"]
