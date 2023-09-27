# ods-pipeline-image

Tekton task for use with [ODS Pipeline](https://github.com/opendevstack/ods-pipeline) to build Go applications.

## Usage

```yaml
tasks:
- name: build
  taskRef:
    resolver: git
    params:
    - { name: url, value: https://github.com/opendevstack/ods-pipeline-image.git }
    - { name: revision, value: main }
    - { name: pathInRepo, value: tasks/package.yaml }
    workspaces:
    - { name: source, workspace: shared-workspace }
```

See the [documentation](https://github.com/opendevstack/ods-pipeline-image/blob/main/docs/package.adoc) for details and available parameters.

## About this repository

`docs` and `tasks` are generated directories from recipes located in `build`. See the `Makefile` target for how everything fits together.
