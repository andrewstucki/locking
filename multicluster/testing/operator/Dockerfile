FROM golang:1.25-alpine as builder

WORKDIR /workspace
COPY . .
WORKDIR /workspace/multicluster/testing/operator
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags="-s -w" -o manager main.go

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /workspace/multicluster/testing/operator/manager /manager

ENTRYPOINT ["/manager"]