#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

module=$(go list -m)
generate() {
  local destination=$1 package=$2 source=$3 interfaces=$4
  shift 4
  go tool mockgen -destination="$destination" -package="$package" "$@" "$module/$source" "$interfaces"
}

generate internal/user/usecase/dependencies_mock_test.go usecase internal/user/usecase UserStore,SessionStore,PasswordHasher,RefreshTokens,TokenIssuer
generate internal/notebook/usecase/dependencies_mock_test.go usecase internal/notebook/usecase Metadata,Files,NotebookCodec
generate internal/runtime/usecase/dependencies_mock_test.go usecase internal/runtime/usecase EnvironmentRepository,JobQueue,CloudProvider,HostConfigurator,AgentClient,Secrets,Clock
for resource in user notebook; do
  generate "internal/$resource/delivery/http/usecase_mock_test.go" delivery "internal/$resource/usecase" Usecase
  generate "internal/$resource/delivery/http/security_mock_test.go" delivery internal/transport/http/security TokenParser
done
generate internal/transport/http/user_mock_test.go httptransport internal/user/usecase Usecase -mock_names=Usecase=MockUserUsecase
generate internal/transport/http/notebook_mock_test.go httptransport internal/notebook/usecase Usecase -mock_names=Usecase=MockNotebookUsecase
generate internal/transport/http/notebook_ports_mock_test.go httptransport internal/notebook/usecase Metadata,Files
generate internal/transport/http/security/dependencies_mock_test.go security internal/transport/http/security TokenParser

# Моки портов пользовательских runtime-сессий для изолированных тестов сценариев.
generate internal/runtimesession/usecase/dependencies_mock_test.go usecase internal/runtimesession/usecase Store,Environments,SessionAgent,OutputWriter
