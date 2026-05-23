import { ApolloServer } from '@apollo/server';
import { gql } from 'graphql-tag';

const typeDefs = gql`
  type Query {
    book(id: ID!): Book
    books: [Book!]!
  }

  type Mutation {
    addBook(title: String!): Book!
  }
`;

const resolvers = {
  Query: {
    book: (_, { id }) => booksById.get(id),
    books: () => Array.from(booksById.values()),
  },
  Mutation: {
    addBook: (_, { title }) => {
      const next = { id: nextID(), title };
      booksById.set(next.id, next);
      return next;
    },
  },
};

export const server = new ApolloServer({ typeDefs, resolvers });
