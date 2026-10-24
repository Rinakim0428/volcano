import { GlobalWorkerOptions } from 'pdfjs-dist/legacy/build/pdf.mjs';

if (typeof globalThis.BOOK_PDF_WORKER !== 'string') {
  throw new Error('PDF 구성 요소가 누락되었습니다. 배포된 HTML 파일을 다시 열어 주세요.');
}
GlobalWorkerOptions.workerSrc = URL.createObjectURL(
  new Blob([globalThis.BOOK_PDF_WORKER], { type: 'text/javascript' }),
);
delete globalThis.BOOK_PDF_WORKER;

class EmbeddedBinaryDataFactory {
  async fetch({ kind, filename }) {
    const collection = globalThis.BOOK_PDF_DATA?.[kind];
    if (!collection || !Object.hasOwn(collection, filename)) {
      throw new Error(`포함되지 않은 PDF 자료입니다: ${kind}/${filename}`);
    }
    return Uint8Array.from(atob(collection[filename]), ch => ch.charCodeAt(0));
  }
}

export const pdfOptions = {
  BinaryDataFactory: EmbeddedBinaryDataFactory,
  cMapPacked: true,
  useWorkerFetch: false,
  useWasm: false,
  isEvalSupported: false,
  disableFontFace: true,
  useSystemFonts: true,
  disableAutoFetch: true,
  stopAtErrors: true,
};
