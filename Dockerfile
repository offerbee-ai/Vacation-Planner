FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/vacation-planner .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=build /out/vacation-planner ./vacation-planner
COPY config/ ./config/
COPY assets/ ./assets/
USER app
EXPOSE 10000
CMD ["./vacation-planner"]
