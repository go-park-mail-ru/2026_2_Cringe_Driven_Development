// Redocly не проверяет default; копируем его в examples, их проверяет no-invalid-schema-examples.
export default function cddPlugin() {
  return {
    id: 'cdd',
    preprocessors: {
      oas3: {
        'default-as-example': () => ({
          Schema(schema) {
            if (!Object.hasOwn(schema, 'default')) return;
            schema.examples = [...(schema.examples ?? []), schema.default];
          },
        }),
      },
    },
  };
}
