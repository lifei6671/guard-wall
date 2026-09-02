FROM golang:1.27-bookworm@sha256:648f440f42a0958804efb24df176f806f9d353b41f1c0627f666428e40310f6b

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

RUN apt-get update \
    && apt-get install --yes --no-install-recommends iproute2 iputils-ping libcap2-bin nftables \
    && rm -rf /var/lib/apt/lists/*

COPY . ./

COPY tests/integration/nftables/run.sh /usr/local/bin/run-nftables-integration
COPY tests/integration/nftables/golden-state.sh /usr/local/bin/run-nftables-golden-state

RUN chmod 0555 /usr/local/bin/run-nftables-integration /usr/local/bin/run-nftables-golden-state

ENV GOCACHE=/tmp/go-build
ENV GOTMPDIR=/tmp

ENTRYPOINT ["/usr/local/bin/run-nftables-integration"]
