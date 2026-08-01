import yaml from 'js-yaml'
import type { ShadowTestFormState } from '@/components/ShadowTestForm'

/** Build a valid engine.shadow-diff.io/v1alpha1 ShadowTest document from form state. */
export function buildShadowTestDoc(form: ShadowTestFormState): Record<string, unknown> {
  return {
    apiVersion: 'engine.shadow-diff.io/v1alpha1',
    kind: 'ShadowTest',
    metadata: {
      name: form.name || 'unnamed',
      namespace: form.namespace || 'default',
    },
    spec: {
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
    },
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
