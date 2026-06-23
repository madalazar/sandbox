FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    intel-cmt-cat \
    util-linux \
    kmod \
 && rm -rf /var/lib/apt/lists/*