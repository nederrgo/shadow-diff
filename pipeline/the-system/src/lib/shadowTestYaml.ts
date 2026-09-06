import yaml from 'js-yaml'
import type { ShadowTestFormState } from '@/components/ShadowTestForm'

/** Build a valid engine.shadow-diff.io/v1alpha1 ShadowTest document from form state. */
function buildShadowTestDoc(form: ShadowTestFormState): Record<string, unknown> {
  const spec: Record<string, unknown> = {
    targetDeployment: form.targetDeployment,
    newImage: form.newImage,
    mode: form.mode,
    storage: {
      type: 's3',
      bucketName: form.s3Bucket,
      region: form.s3Region,
      credentialsSecretRef: {
        name: form.s3Secret,
      },
      retentionPolicy: form.retentionPolicy,
    },
  }

  const dependencies = form.dependencies
    .filter((d) => d.name.trim() && d.type.trim() && d.envVarInjection.trim())
    .map((d) => {
      const entry: Record<string, unknown> = {
        name: d.name.trim(),
        type: d.type.trim(),
        envVarInjection: d.envVarInjection.trim(),
      }
      if (d.image.trim()) entry.image = d.image.trim()
      const port = Number.parseInt(d.port, 10)
      if (Number.isFinite(port) && port > 0) entry.port = port
      return entry
    })
  if (dependencies.length > 0) {
    spec.dependencies = dependencies
  }

  const inp = form.input
  if (inp.driver === 'rabbitmq_message') {
    const amqp: Record<string, unknown> = {
      prodUrl: inp.prodUrl.trim(),
      credentialsSecretRef: {
        name: inp.amqpSecret.trim(),
      },
      exchange: inp.exchange.trim(),
      routingKey: inp.routingKey.trim() || '#',
      targetDependency: inp.targetDependency.trim(),
    }
    if (inp.exchangeType.trim()) amqp.exchangeType = inp.exchangeType.trim()
    spec.inputs = [{ driver: 'rabbitmq_message', amqp }]
  } else if (inp.driver === 'http_request') {
    const port = Number.parseInt(inp.port, 10)
    if (Number.isFinite(port) && port > 0) {
      spec.inputs = [{ driver: 'http_request', port }]
    }
  }

  return {
    apiVersion: 'engine.shadow-diff.io/v1alpha1',
    kind: 'ShadowTest',
    metadata: {
      name: form.name || 'unnamed',
      namespace: form.namespace || 'default',
    },
    spec,
  }
}

export function dumpShadowTestYaml(form: ShadowTestFormState): string {
  return yaml.dump(buildShadowTestDoc(form), {
    indent: 2,
    lineWidth: 100,
    noRefs: true,
    sortKeys: false,
  })
}
