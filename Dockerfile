FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/morpheus-csi ./cmd/morpheus-csi

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/morpheus-csi /morpheus-csi

ENTRYPOINT ["/morpheus-csi"]
