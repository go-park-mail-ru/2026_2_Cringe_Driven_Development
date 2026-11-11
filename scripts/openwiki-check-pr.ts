// Свежий чекпоинт относительно base не покрывает правки исходников внутри PR.
try {
    const [base, head, ...extra] = process.argv.slice(2);
    const fullSha = /^[a-f0-9]{40}$/i;
    if (!base || !head || extra.length > 0 || !fullSha.test(base) || !fullSha.test(head)) {
        throw new Error(
            'Usage: bun scripts/openwiki-check-pr.ts <base SHA> <head SHA> (full SHAs required)',
        );
    }
    const proc = Bun.spawn(
        ['git', 'diff', '--name-only', '-z', '--no-renames', `${base}...${head}`, '--'],
        {
            stdout: 'pipe',
            stderr: 'pipe',
        },
    );
    const [stdout, stderr, exitCode] = await Promise.all([
        new Response(proc.stdout).text(),
        new Response(proc.stderr).text(),
        proc.exited,
    ]);
    if (exitCode !== 0)
        throw new Error(`PR path comparison failed (${exitCode}): ${stderr.trim()}`);
    const outside = stdout
        .split('\0')
        .filter(
            (path) =>
                path.length > 0 &&
                (!path.startsWith('openwiki/') || path === 'openwiki/INSTRUCTIONS.md'),
        );
    if (outside.length > 0) {
        throw new Error(
            `Wiki PR changes source paths (outside generated openwiki/): ${outside.map((path) => JSON.stringify(path)).join(', ')}`,
        );
    }
    console.log('PR paths: wiki-only');
} catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
}
