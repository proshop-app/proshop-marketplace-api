# Сборка в образе Go той же версии, что у разработчика (go1.23.2), запуск —
# статический бинарник на scratch от непривилегированного пользователя.
# Базовый образ закреплён тегом и дайджестом; реестр — BASE_REGISTRY.
ARG BASE_REGISTRY=docker.io

FROM ${BASE_REGISTRY}/library/golang:1.23.2-alpine@sha256:9dd2625a1ff2859b8d8b01d8f7822c0f528942fe56cfe7a1e7c38d3b8d72d679 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local

COPY go.mod go.sum ./
RUN go mod download

COPY deploy/reference /reference
RUN find /reference -type d -exec chmod 0555 {} \; \
 && find /reference -type f -exec chmod 0444 {} \;

COPY cmd ./cmd
COPY internal ./internal
COPY pkg ./pkg
RUN go build -trimpath -ldflags="-s -w" -o /out/marketplace ./cmd/marketplace \
 && go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate \
 && go build -trimpath -ldflags="-s -w" -o /out/taxonomy ./cmd/taxonomy

FROM scratch AS taxonomy
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/taxonomy /taxonomy
USER 65532:65532
ENTRYPOINT ["/taxonomy"]
CMD ["check"]

FROM scratch AS runtime
COPY --from=build --chown=65532:65532 /reference /reference
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/marketplace /out/migrate /out/taxonomy /
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/marketplace"]
