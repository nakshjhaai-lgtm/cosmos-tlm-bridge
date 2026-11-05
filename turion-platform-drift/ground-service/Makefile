# Ground-service deploy targets (AWS-only).
AWS_REGION ?= us-west-2

deploy:
	aws ecs update-service --cluster ground-eks --service ground-telemetry-api --region $(AWS_REGION)

register-job:
	aws batch register-job-definition --cli-input-json file://batch/job-definition.json

deploy-queue:
	aws batch create-job-queue --job-queue-name droid-pass-queue

synth-infra:
	terraform -chdir=infra apply -auto-approve

.PHONY: deploy register-job deploy-queue synth-infra
