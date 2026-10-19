FROM golang:1.23-alpine AS build

WORKDIR /src
COPY . .
RUN go build -trimpath -ldflags='-s -w' -o /out/transfer ./services/transfer

FROM alpine:3.21
RUN addgroup -S transfer && adduser -S -G transfer transfer
COPY --from=build /out/transfer /usr/local/bin/transfer
USER transfer
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/transfer"]
