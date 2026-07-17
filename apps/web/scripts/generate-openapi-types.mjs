import { mkdir, readFile, writeFile } from "node:fs/promises"
import { fileURLToPath } from "node:url"

import openapiTS, {
  astToString,
  COMMENT_HEADER,
} from "openapi-typescript"

const arguments_ = process.argv.slice(2)
const check = arguments_.length === 1 && arguments_[0] === "--check"

if (arguments_.length > 0 && !check) {
  throw new Error(
    `Unsupported arguments: ${arguments_.join(" ")}. Only --check is supported.`,
  )
}

const contractURL = new URL("../../../contracts/openapi.yaml", import.meta.url)
const outputURL = new URL(
  "../app/types/openapi.generated.ts",
  import.meta.url,
)
const outputPath = fileURLToPath(outputURL)
const ast = await openapiTS(contractURL, {
  alphabetize: true,
  emptyObjectsUnknown: true,
})
const generated = `${COMMENT_HEADER}${astToString(ast)}`

if (check) {
  let current
  try {
    current = await readFile(outputURL, "utf8")
  } catch (error) {
    if (error instanceof Error && "code" in error && error.code === "ENOENT") {
      console.error(
        `${outputPath} is missing. Run "make generate-api-types".`,
      )
      process.exitCode = 1
    } else {
      throw error
    }
  }

  if (current !== undefined && current !== generated) {
    console.error(
      `${outputPath} is stale. Run "make generate-api-types".`,
    )
    process.exitCode = 1
  }
} else {
  await mkdir(new URL("../app/types/", import.meta.url), { recursive: true })
  await writeFile(outputURL, generated, "utf8")
}
