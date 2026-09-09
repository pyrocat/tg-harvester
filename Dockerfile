FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tg-harvester ./cmd/tg-harvester

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tg-harvester /tg-harvester
VOLUME /data
ENV TGH_LISTEN_ADDR=:8080 \
    TGH_DB_PATH=/data/tg-harvester.db
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/tg-harvester"]
