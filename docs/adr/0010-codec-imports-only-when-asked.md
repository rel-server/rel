# The generated client imports a package only for the codec options asked for

The codec chunks of the served `database.ts` import their packages (`cbor-x`, `lossless-json`), and the generator hoists those imports to the top of the file only for the options requested. The default file has no import at all : a consumer who wants no dependency asks for no codec option.
