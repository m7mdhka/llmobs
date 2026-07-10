# langfuse-compat plugin backend (Python). Built + run by the operator; the kernel
# reaches it by URL (external-URL executor). No kernel internals.
FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY *.py ./
EXPOSE 8080
CMD ["uvicorn", "app:app", "--host", "0.0.0.0", "--port", "8080"]
