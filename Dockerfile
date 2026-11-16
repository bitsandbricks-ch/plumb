# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /plumb ./cmd/plumb

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /plumb /plumb
USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/plumb"]
