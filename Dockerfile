FROM golang:1.23-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/morpheus-csi ./cmd/morpheus-csi

FROM alpine:3.20

RUN apk add --no-cache e2fsprogs util-linux xfsprogs

COPY --from=build /out/morpheus-csi /morpheus-csi

ENTRYPOINT ["/morpheus-csi"]
