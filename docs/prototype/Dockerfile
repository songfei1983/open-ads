FROM python:3.13-slim

WORKDIR /app
COPY . /app

USER nobody:nogroup
EXPOSE 4173

CMD ["python", "-m", "http.server", "4173", "--bind", "0.0.0.0", "--directory", "/app"]
