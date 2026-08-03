FROM python:3.13.7-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY server.py .
RUN mkdir -p /data && chown -R 65532:65532 /data
USER 65532:65532
EXPOSE 8081 9091
CMD ["python", "server.py"]
