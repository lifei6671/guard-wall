from pathlib import Path
import re
import unittest


ROOT = Path(__file__).resolve().parents[1]


def read(relative_path: str) -> str:
    return (ROOT / relative_path).read_text(encoding="utf-8")


class TaskContinuityContractTests(unittest.TestCase):
    def test_first_adoption_requires_explicit_invocation(self) -> None:
        skill = read("SKILL.md")

        self.assertIn("首次启用必须来自显式调用", skill)
        self.assertIn("HANDOFF 不存在，且只是隐式匹配", skill)
        self.assertIn("不得创建 `.task-memory`、不得修改项目指令", skill)

    def test_later_automatic_recovery_remains_available(self) -> None:
        metadata = read("agents/openai.yaml")
        skill = read("SKILL.md")

        self.assertRegex(metadata, r"(?m)^\s*allow_implicit_invocation:\s*true\s*$")
        self.assertIn("有效 HANDOFF + 生效的 managed block", skill)
        self.assertIn("不要求用户再次显式调用", skill)

    def test_first_adoption_of_existing_project_is_read_only_until_confirmed(self) -> None:
        adoption = read("references/adoption.md")

        self.assertIn("先做只读发现", adoption)
        self.assertIn("交互式澄清并等待确认", adoption)
        self.assertIn("在用户明确确认契约前保持只读", adoption)
        self.assertIn("用户已确认", adoption)
        self.assertIn("仓库事实", adoption)
        self.assertIn("推断", adoption)
        self.assertIn("未知", adoption)

    def test_managed_block_is_unique_and_handles_invalid_handoff(self) -> None:
        block = read("templates/AGENTS.task-continuity.md")

        self.assertEqual(block.count("<!-- TASK_CONTINUITY:BEGIN -->"), 1)
        self.assertEqual(block.count("<!-- TASK_CONTINUITY:END -->"), 1)
        self.assertIn("$task-continuity", block)
        self.assertIn("用户无需再次显式要求恢复", block)
        self.assertIn("含模板占位符，不得声称已经恢复", block)

    def test_local_markdown_links_resolve(self) -> None:
        documents = [
            "SKILL.md",
            "README.md",
            "references/adoption.md",
            "references/memory-protocol.md",
            "references/topic-shift.md",
        ]
        pattern = re.compile(r"\[[^\]]+\]\((?!https?://|#)([^)]+)\)")

        missing: list[str] = []
        for document in documents:
            source = read(document)
            base = (ROOT / document).parent
            for match in pattern.finditer(source):
                target = match.group(1).split("#", 1)[0]
                if not (base / target).exists():
                    missing.append(f"{document} -> {target}")

        self.assertEqual(missing, [])


if __name__ == "__main__":
    unittest.main()
