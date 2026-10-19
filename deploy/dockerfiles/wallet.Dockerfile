FROM golang:1.23-alpine AS build

WORKDIR /src
COPY . .
RUN go build -trimpath -ldflags='-s -w' -o /out/wallet ./services/wallet

FROM alpine:3.21
RUN addgroup -S wallet && adduser -S -G wallet wallet
COPY --from=build /out/wallet /usr/local/bin/wallet
USER wallet
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/wallet"]
