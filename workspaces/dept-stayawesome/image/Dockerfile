# Workspace image for tenant `dept-stayawesome`.
# Pre-bakes Claude Code + gt-plan so cold-starts stay fast and the agent
# startup_script doesn't need network during boot.

FROM debian:bookworm-slim

ARG NODE_MAJOR=20
ARG GT_PLAN_VERSION=latest

ENV DEBIAN_FRONTEND=noninteractive \
    LANG=C.UTF-8 \
    SHELL=/bin/bash

RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git gnupg jq sudo openssh-client \
    && curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*

RUN npm install -g @anthropic-ai/claude-code \
    && npm install -g @stayawesome/gt-plan@${GT_PLAN_VERSION} \
    && npm cache clean --force

# Unprivileged user expected by main.tf (user = "1000:1000").
RUN useradd --create-home --uid 1000 --shell /bin/bash coder

USER coder
WORKDIR /home/coder
