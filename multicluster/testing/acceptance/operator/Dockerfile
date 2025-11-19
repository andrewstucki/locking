FROM golang:1.25-alpine AS builder

WORKDIR /workspace
COPY . .
WORKDIR /workspace/multicluster/testing/acceptance/operator
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags="-s -w" -o manager

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /workspace/multicluster/testing/acceptance/operator/manager /manager

ENTRYPOINT ["/manager"]