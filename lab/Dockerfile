# Reference service for the calibration lab (see README.md).
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/work ./cmd/work
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /work ./cmd/work

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /work /work
EXPOSE 8080
ENTRYPOINT ["/work"]
