FROM golang:1.24.0-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/search-workload ./cmd/search-workload

FROM gcr.io/distroless/base-debian12:nonroot
COPY --from=build /out/search-workload /usr/local/bin/search-workload
ENTRYPOINT ["/usr/local/bin/search-workload"]
