import { readFile, stat } from 'node:fs/promises';
import { join } from 'node:path';

function isWikiSourcePath(path: string): boolean {
    if (
        !path ||
        /(^|\/)(node_modules|dist|coverage|bin|mocks|\.git)(\/|$)/.test(path) ||
        /(^|\/)\.env(?:[./]|$)/.test(path) ||
        /\.(pem|key|p12|pfx)$/.test(path) ||
        /(^|\/)mock[^/]*\.go$|_mock_test\.go$/.test(path) ||
        ['internal/transport/http/api/api.gen.go', 'internal/transport/http/api/openapi.yaml'].includes(path)
    )
        return false;
    return (
        [
            'README.md',
            'AGENTS.md',
            '.openwikiignore',
            'openwiki/INSTRUCTIONS.md',
            'docs/authentication.md',
            'docs/input-security.md',
            'docs/redis-sessions.md',
            'docs/local-development.md',
            'docs/release.md',
            'docs/openapi.md',
            'go.mod',
            'go.sum',
            'Makefile',
            'docker-compose.yml',
            'docker-compose.override.yml',
            '.golangci.yml',
            '.editorconfig',
            '.gitignore',
            '.dockerignore',
        ].includes(path) ||
        /^(internal|cmd|migrations|build|scripts)\//.test(path) ||
        path.startsWith('.github/workflows/')
    );
}

type Checkpoint = { gitHead: string; status: 'complete'; updatedAt?: string };
type Freshness = { sourceSha: string; targetSha: string; changedPaths: string[]; fresh: boolean };
const fullSha = /^[a-f0-9]{40}$/i;

async function readCheckpoint(root: string): Promise<Checkpoint> {
    const quickstart = join(root, 'openwiki/quickstart.md');
    if (!(await stat(quickstart)).isFile()) throw new Error(`Missing wiki page: ${quickstart}`);
    const path = join(root, 'openwiki/.last-update.json');
    const value: unknown = JSON.parse(await readFile(path, 'utf8'));
    if (
        !value ||
        typeof value !== 'object' ||
        !('status' in value) ||
        value.status !== 'complete'
    ) {
        throw new Error(`${path}: status must be complete`);
    }
    if (
        !('gitHead' in value) ||
        typeof value.gitHead !== 'string' ||
        !fullSha.test(value.gitHead)
    ) {
        throw new Error(`${path}: gitHead must be a full 40-character SHA`);
    }
    return {
        gitHead: value.gitHead,
        status: 'complete',
        updatedAt:
            'updatedAt' in value && typeof value.updatedAt === 'string'
                ? value.updatedAt
                : undefined,
    };
}

async function git(root: string, args: string[]): Promise<string> {
    const proc = Bun.spawn(['git', ...args], { cwd: root, stdout: 'pipe', stderr: 'pipe' });
    const [stdout, stderr, exitCode] = await Promise.all([
        new Response(proc.stdout).text(),
        new Response(proc.stderr).text(),
        proc.exited,
    ]);
    if (exitCode !== 0)
        throw new Error(`git ${args.join(' ')} failed (${exitCode}): ${stderr.trim()}`);
    return stdout;
}

/** Проверяет завершённый checkpoint и изменения разрешённых исходников до targetSha. */
export async function checkOpenWiki(root: string, targetSha: string): Promise<Freshness> {
    if (!fullSha.test(targetSha))
        throw new Error('Target must be a full 40-character SHA (--against <SHA>)');
    const { gitHead: sourceSha } = await readCheckpoint(root);
    // Отклоняем объекты, не являющиеся коммитами, до проверки предков и сравнения деревьев.
    await git(root, ['rev-parse', '--verify', `${sourceSha}^{commit}`]);
    await git(root, ['rev-parse', '--verify', `${targetSha}^{commit}`]);
    await git(root, ['merge-base', '--is-ancestor', sourceSha, targetSha]);
    const paths = await git(root, [
        'diff',
        '--name-only',
        '-z',
        '--no-renames',
        sourceSha,
        targetSha,
        '--',
    ]);
    const changedPaths = paths.split('\0').filter(isWikiSourcePath);
    return { sourceSha, targetSha, changedPaths, fresh: changedPaths.length === 0 };
}

if (import.meta.main) {
    try {
        const args = process.argv.slice(2);
        if (args.length !== 2 || args[0] !== '--against' || !args[1]) {
            throw new Error('Usage: bun scripts/openwiki-check.ts --against <SHA>');
        }
        const root = process.cwd();
        const result = await checkOpenWiki(root, args[1]);
        const checkpoint = await readCheckpoint(root);
        console.log(`Source SHA: ${result.sourceSha}`);
        console.log(`Target SHA: ${result.targetSha}`);
        console.log(`Updated at (observation only): ${checkpoint.updatedAt ?? 'unknown'}`);
        console.log(`Wiki: ${result.fresh ? 'fresh' : 'stale'}`);
        if (result.changedPaths.length > 0) {
            console.log('Changed source paths:');
            for (const path of result.changedPaths) console.log(JSON.stringify(path));
        }
        process.exitCode = result.fresh ? 0 : 1;
    } catch (error) {
        console.error(error instanceof Error ? error.message : String(error));
        process.exitCode = 1;
    }
}
