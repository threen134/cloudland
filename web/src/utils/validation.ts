export const NAME_REGEX = /^[a-zA-Z][a-zA-Z0-9_-]*$/

export const isValidName = (name: string): boolean => {
    if (!name) return true
    return NAME_REGEX.test(name)
}
