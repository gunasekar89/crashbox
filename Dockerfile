FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/gunasekar89/crashbox/internal/collect.Version=${VERSION}" -o /crashbox ./cmd/crashbox

FROM gcr.io/distroless/static:nonroot
COPY --from=build /crashbox /crashbox
ENTRYPOINT ["/crashbox"]
