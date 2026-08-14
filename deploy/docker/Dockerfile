FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/gerry ./cmd/gerry

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/gerry /usr/local/bin/gerry
VOLUME /data
ENTRYPOINT ["gerry"]
CMD ["serve", "--config", "/etc/gerry.yaml"]
