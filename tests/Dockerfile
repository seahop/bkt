FROM python:3.12-alpine

# Install all tools the smoke tests need
RUN apk add --no-cache \
    bash \
    curl \
    zip \
    coreutils \
    && pip install --no-cache-dir awscli --quiet

WORKDIR /tests

COPY smoke.sh .

RUN chmod +x smoke.sh

ENTRYPOINT ["bash", "smoke.sh"]
