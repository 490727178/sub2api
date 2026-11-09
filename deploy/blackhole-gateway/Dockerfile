FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /blackhole-gateway .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /blackhole-gateway /blackhole-gateway
EXPOSE 8788
ENTRYPOINT ["/blackhole-gateway"]
